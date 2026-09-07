import type { ConnectitConnector } from '@memohai/sdk'

// Local UI fixture based on the supplied production catalog screenshot.
// No production account or connection state is copied.
export const connectorPreviewCatalog: ConnectitConnector[] = [
  { type: 'github', name: 'GitHub', icon_url: 'https://cdn.simpleicons.org/github/888888' },
  { type: 'googledrive', name: 'Google Drive', icon_url: 'https://cdn.simpleicons.org/googledrive' },
  { type: 'gmail', name: 'Gmail', icon_url: 'https://cdn.simpleicons.org/gmail' },
  { type: 'googlecalendar', name: 'Google Calendar', icon_url: 'https://cdn.simpleicons.org/googlecalendar' },
  { type: 'notion', name: 'Notion', icon_url: 'https://cdn.simpleicons.org/notion/888888' },
  { type: 'slack', name: 'Slack', icon_url: 'slack' },
  { type: 'linear', name: 'Linear', icon_url: 'https://cdn.simpleicons.org/linear' },
  { type: 'googlesheets', name: 'Google Sheets', icon_url: 'https://cdn.simpleicons.org/googlesheets' },
  { type: 'googledocs', name: 'Google Docs', icon_url: 'https://cdn.simpleicons.org/googledocs' },
  { type: 'youtube', name: 'YouTube', icon_url: 'https://cdn.simpleicons.org/youtube' },
  { type: 'dropbox', name: 'Dropbox', icon_url: 'https://cdn.simpleicons.org/dropbox' },
]
