import { describe, expect, it } from 'vitest'
import { botCheckDetail } from './bot-check'

const translate = (key: string) => `t:${key}`

describe('botCheckDetail', () => {
  it('renders the catalog copy when the check carries a setup error code', () => {
    expect(botCheckDetail({ detail: 'old text', metadata: { setup_error_code: 'workspace.image_not_found' } }, translate))
      .toBe('t:errors.workspace.image_not_found')
  })

  it('shows the text of an earlier server when the check has no code', () => {
    expect(botCheckDetail({ detail: ' pull access denied ', metadata: { setup_error_phase: 'image_prepare', setup_error_code: '' } }, translate))
      .toBe('pull access denied')
    expect(botCheckDetail({}, translate)).toBe('')
  })
})
