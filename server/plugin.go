package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
)

// Admin-configurable settings (from plugin.json)
type Config struct {
	AutoUnfurl  bool
	UserCountry string
}

// Plugin implements the Mattermost plugin interface.
type Plugin struct {
	plugin.MattermostPlugin

	cfg        *Config
	httpClient *http.Client
	urlRegex   *regexp.Regexp
}

// NewPlugin ensures everything is initialised even if OnActivate changes later.
func NewPlugin() *Plugin {
	return &Plugin{
		httpClient: &http.Client{Timeout: 8 * time.Second},
		urlRegex:   regexp.MustCompile(`https?://[^\s]+`),
	}
}

func (p *Plugin) OnConfigurationChange() error {
	var c Config
	if err := p.API.LoadPluginConfiguration(&c); err != nil {
		return err
	}
	p.cfg = &c
	return nil
}

func (p *Plugin) OnActivate() error {
	// Belt-and-braces: make sure these are set even if NewPlugin wasn’t used.
	if p.httpClient == nil {
		p.httpClient = &http.Client{Timeout: 8 * time.Second}
	}
	if p.urlRegex == nil {
		p.urlRegex = regexp.MustCompile(`https?://[^\s]+`)
	}
	// Register /songlink slash command
	return p.registerCommands()
}

// ---- Slash command ----

func (p *Plugin) registerCommands() error {
	cmd := &model.Command{
		Trigger:          "songlink",
		AutoComplete:     true,
		AutoCompleteDesc: "Create a smart music preview from a URL. Usage: /songlink <url>",
		DisplayName:      "Songlink",
	}
	if appErr := p.API.RegisterCommand(cmd); appErr != nil {
		return appErr
	}
	return nil
}

func (p *Plugin) ExecuteCommand(ctx *plugin.Context, args *model.CommandArgs) (*model.CommandResponse, *model.AppError) {
	// Never let a panic kill the plugin process.
	defer func() {
		if r := recover(); r != nil {
			p.API.LogError("panic in ExecuteCommand", "recover", r)
		}
	}()
	if args == nil || strings.TrimSpace(args.Command) == "" {
		return &model.CommandResponse{
			ResponseType: model.CommandResponseTypeEphemeral,
			Text:         "Usage: /songlink <music-url>",
		}, nil
	}

	parts := strings.Fields(args.Command)
	if len(parts) < 2 {
		return &model.CommandResponse{
			ResponseType: model.CommandResponseTypeEphemeral,
			Text:         "Usage: /songlink <music-url>",
		}, nil
	}
	musicURL := cleanMusicURL(parts[1])

	// Kick work to background so the UI clears instantly.
	userID := args.UserId
	channelID := args.ChannelId
	go func(url string) {
		att, err := p.lookupOdesli(url)
		if err != nil || att == nil {
			// Tell the user quietly if it fails.
			p.API.SendEphemeralPost(userID, &model.Post{
				ChannelId: channelID,
				Message:   "Couldn’t fetch details for that link.",
			})
			if err != nil {
				p.API.LogError("odesli lookup failed", "err", err.Error())
			}
			return
		}
		// Post the result as the invoking user (no channel-join fuss).
		post := &model.Post{
			UserId:    userID,
			ChannelId: channelID,
			Props: map[string]any{
				"attachments": []*model.SlackAttachment{att},
			},
		}
		if _, appErr := p.API.CreatePost(post); appErr != nil {
			p.API.SendEphemeralPost(userID, &model.Post{
				ChannelId: channelID,
				Message:   "Failed to post preview.",
			})
			p.API.LogError("CreatePost failed", "err", appErr.Error())
		}
	}(musicURL)

	// Immediate, lightweight response — clears the input and shows a hint.
	return &model.CommandResponse{
		ResponseType: model.CommandResponseTypeEphemeral,
		Text:         "Fetching preview…",
	}, nil
}

// ---- Optional unfurl on paste ----

func (p *Plugin) MessageWillBePosted(ctx *plugin.Context, post *model.Post) (*model.Post, string) {
	if p.cfg == nil || !p.cfg.AutoUnfurl {
		return post, ""
	}
	if post == nil || p.urlRegex == nil {
		return post, ""
	}

	urls := p.urlRegex.FindAllString(post.Message, -1)
	if len(urls) == 0 {
		return post, ""
	}

	att, err := p.lookupOdesli(urls[0])
	if err != nil || att == nil {
		return post, ""
	}

	// Reply in thread via bot
	botID := p.ensureBot()
	reply := &model.Post{
		UserId:    botID,
		ChannelId: post.ChannelId,
		RootId:    post.Id,
		Props: map[string]any{
			"attachments": []*model.SlackAttachment{att},
		},
	}
	if _, appErr := p.API.CreatePost(reply); appErr != nil {
		p.API.LogWarn("failed to create unfurl post", "err", appErr.Error())
	}
	return post, ""
}

// ---- Odesli client ----

type odesliResponse struct {
	EntityUniqueId     string `json:"entityUniqueId"`
	PageUrl            string `json:"pageUrl"`
	EntitiesByUniqueId map[string]struct {
		Title        string `json:"title"`
		ArtistName   string `json:"artistName"`
		ThumbnailUrl string `json:"thumbnailUrl"`
	} `json:"entitiesByUniqueId"`
	LinksByPlatform map[string]struct {
		Url string `json:"url"`
	} `json:"linksByPlatform"`
}

func (p *Plugin) lookupOdesli(musicURL string) (*model.SlackAttachment, error) {
	if p.httpClient == nil {
		return nil, fmt.Errorf("http client not initialised")
	}
	if strings.TrimSpace(musicURL) == "" {
		return nil, fmt.Errorf("empty url")
	}

	q := url.Values{"url": {musicURL}}
	if p.cfg != nil && strings.TrimSpace(p.cfg.UserCountry) != "" {
		q.Set("userCountry", strings.TrimSpace(p.cfg.UserCountry))
	}
	api := "https://api.song.link/v1-alpha.1/links?" + q.Encode()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, api, nil)
	req.Header.Set("User-Agent", "Mattermost-Songlink-Plugin/0.1")

	res, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("odesli status %d", res.StatusCode)
	}

	var o odesliResponse
	if err := json.NewDecoder(res.Body).Decode(&o); err != nil {
		return nil, err
	}

	// Build attachment safely
	title := "Track"
	artist := ""
	if ent, ok := o.EntitiesByUniqueId[o.EntityUniqueId]; ok {
		if strings.TrimSpace(ent.Title) != "" {
			title = ent.Title
		}
		artist = ent.ArtistName
	}

	att := &model.SlackAttachment{
		Fallback:  strings.TrimSpace(fmt.Sprintf("%s — %s", artist, title)),
		Title:     strings.TrimSpace(fmt.Sprintf("%s — %s", artist, title)),
		TitleLink: o.PageUrl,
	}
	if ent, ok := o.EntitiesByUniqueId[o.EntityUniqueId]; ok && strings.TrimSpace(ent.ThumbnailUrl) != "" {
		att.ThumbURL = ent.ThumbnailUrl
	}

	// Add a few platform buttons inline
	var chips []string
    allowed := []string{
        "spotify",
        "itunes",
        "appleMusic",
        "youtubeMusic",
        "qobuz",
        "tidal",
        "amazonMusic",
        "soundcloud",
        "bandcamp",
    }
    labels := map[string]string{
        "spotify":      "Spotify",
        "itunes":       "iTunes",
        "appleMusic":   "Apple Music",
        "youtubeMusic": "YouTube Music",
        "qobuz":       "Qobuz",
        "tidal":        "TIDAL",
        "amazonMusic":  "Amazon Music",
        "soundcloud":   "SoundCloud",
        "bandcamp":     "Bandcamp",
    }
    for _, k := range allowed {
        if v, ok := o.LinksByPlatform[k]; ok && v.Url != "" {
            chips = append(chips, fmt.Sprintf("[%s](%s)", labels[k], v.Url))
        }
    }
	if len(chips) > 0 {
		att.Text = strings.Join(chips, " • ")
	}
	return att, nil
}

func cleanMusicURL(s string) string {
	s = strings.TrimSpace(s)
	// Strip surrounding angle brackets often added by chat clients
	s = strings.Trim(s, "<>")
	// Trim common trailing punctuation
	for len(s) > 0 {
		last := s[len(s)-1]
		if last == ')' || last == '.' || last == ',' || last == ']' || last == '>' {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	// Ensure scheme is present
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		s = "https://" + s
	}
	return s
}

// ---- Helpers ----

func (p *Plugin) textResponse(msg string) *model.CommandResponse {
	return &model.CommandResponse{
		ResponseType: model.CommandResponseTypeEphemeral,
		Text:         msg,
	}
}

func (p *Plugin) ensureBot() string {
	id, err := p.API.EnsureBotUser(&model.Bot{
		Username:    "songlink",
		DisplayName: "Songlink",
		Description: "Smart music links by Odesli",
	})
	if err != nil {
		p.API.LogWarn("EnsureBotUser failed", "err", err.Error())
		return ""
	}
	return id
}