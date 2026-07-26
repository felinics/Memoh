<script lang="ts">
// The implementation now lives in @felinic/ui — this shim keeps the existing
// relative imports working; migrate call sites to import { ConfirmDeleteDialog }
// from '@felinic/ui' and delete this file. NOTE: the library version requires
// cancelLabel/confirmLabel props (no i18n fallback).
import { ConfirmDeleteDialog } from '@felinic/ui'

export default ConfirmDeleteDialog
</script>
