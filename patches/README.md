# Dependency patches

This directory contains pnpm patches that are applied during install. Treat each
patch as a local upstream delta: keep it small, documented, and removable.

## reka-ui@2.10.1.patch

Why:

`reka-ui@2.10.1` reads `event.currentTarget` after `await nextTick()` inside
`MenuItemImpl` (`onFocus`) and `SelectItem` (`handlePointerMove` /
`handlePointerLeave`). Standard DOM dispatch clears `event.currentTarget` to
`null` once synchronous propagation finishes, so reading it after an `await`
drops hover/focus highlight updates.

Scope:

Captures `const item = event.currentTarget` synchronously before `await nextTick()`
in both ESM/CJS runtime entries (`dist/Menu/MenuItemImpl.{js,cjs}`,
`dist/Select/SelectItem.{js,cjs}`) and source files.

Remove when:

`reka-ui` upstream captures `event.currentTarget` before awaiting `nextTick()`.
