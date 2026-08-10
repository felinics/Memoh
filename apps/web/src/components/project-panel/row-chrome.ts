// Trailing row actions stay hidden until the row is hovered (or the button
// itself takes keyboard focus), so the tree reads calm at rest while creation
// stays one click away rather than a right-click away. Shared by every row in
// the panel so the reveal behaves identically everywhere.
export const rowActionClass = 'size-6 shrink-0 p-0 text-muted-foreground opacity-0 focus-visible:opacity-100 group-hover/row:opacity-100' /* ui-allow-style */
