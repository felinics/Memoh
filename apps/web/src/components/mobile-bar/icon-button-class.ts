// The mobile bar's icon-button chrome, shared by MobileBarIconButton (plain
// clicks) and any reka `as-child` trigger (menus/popovers), which cannot use
// the component — see the warning in icon-button.vue. Muted at rest →
// foreground on hover, stay-lit while its menu is open (data-[state=open] is
// set by reka only when the button is a menu trigger).
export const mobileBarIconButtonClass = 'shrink-0 text-muted-foreground hover:text-foreground data-[state=open]:text-foreground' /* ui-allow-style */
