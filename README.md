# Songlink (Odesli) for Mattermost

Smart music links for Mattermost. Converts Spotify/Apple Music/YouTube/etc URLs into universal Odesli links with rich previews. Includes:

- /songlink slash command
- Optional auto-unfurl of supported music links

## Build

Requirements: Go 1.22+

- make
- Upload the resulting tar.gz in `dist/` to System Console → Plugins → Upload

## Configuration

- AutoUnfurl: whether to post automatic previews when music links are shared
- UserCountry: optional country code to localize link availability

## Usage

- /songlink <url or search query>

## Notes

- Uses Odesli public API (https://api.song.link)
