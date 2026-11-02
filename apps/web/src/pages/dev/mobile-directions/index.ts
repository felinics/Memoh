// Entry for the dev-only mobile direction comparison. main.ts imports this
// module only in dev builds, so none of it ships to production. It is not
// behind the `memoh:dev-tools` flag: the comparison is meant to be opened on a
// phone over the LAN, where nobody has set that flag, and the switcher only
// renders at mobile width.
import { createApp } from 'vue'
import './mobile-directions.css'
import { installPressFeel } from './press-feel'
import { syncChoices } from './state'
import Switcher from './switcher.vue'

export function installMobileDirections(search: string) {
  syncChoices(search)
  installPressFeel()
  const host = document.createElement('div')
  document.body.append(host)
  createApp(Switcher).mount(host)
}
