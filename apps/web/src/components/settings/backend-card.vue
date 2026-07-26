<script lang="ts">
// The implementation now lives in @felinic/ui (src/components/settings) — this
// shim keeps the existing relative imports working; migrate call sites to
// import { BackendCard } from '@felinic/ui' and delete this file.
import { BackendCard } from '@felinic/ui'

export default BackendCard
</script>
