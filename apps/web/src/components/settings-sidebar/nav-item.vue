<script lang="ts">
// The implementation now lives in @felinic/ui (src/components/settings) so the
// component showcase composes the same owner instead of transcribing it. This
// shim keeps the existing relative imports working; migrate call sites to
// import { NavItem } from '@felinic/ui' and delete this file.
import { NavItem } from '@felinic/ui'

export default NavItem
</script>
