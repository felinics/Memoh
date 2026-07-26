<script lang="ts">
// The implementation now lives in @felinic/ui — this shim keeps the existing
// relative imports working; migrate call sites to import { ConfirmPopover }
// from '@felinic/ui' and delete this file. NOTE: the library version requires
// cancelText/confirmText props (no i18n fallback).
import { ConfirmPopover } from '@felinic/ui'

export default ConfirmPopover
</script>
