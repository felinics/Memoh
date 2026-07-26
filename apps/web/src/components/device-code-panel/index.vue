<script lang="ts">
// The implementation now lives in @felinic/ui — this shim keeps the existing
// relative imports working; migrate call sites to import { DeviceCodePanel }
// from '@felinic/ui' and delete this file. NOTE: the library version takes all
// copy as props (copyAndOpenLabel / retryLabel / expiredLabel / expiresInLabel
// / copyFailedMessage) — callers must pass them (see the 3 call sites already
// migrated in this branch).
import { DeviceCodePanel } from '@felinic/ui'

export default DeviceCodePanel
</script>
