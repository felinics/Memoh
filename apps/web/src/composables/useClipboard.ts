// The implementation now lives in @felinic/ui (src/lib/clipboard) — this shim
// keeps the existing relative imports working; migrate call sites to
// import { useClipboard } from '@felinic/ui' and delete this file.
export { useClipboard } from '@felinic/ui'
