<template>
  <!-- 几何居中 + 光学上移(与 SaaS 登录同语言);无 OAuth / 多步,仅 username + password。 -->
  <main
    class="relative flex h-screen w-screen flex-col items-center justify-center overflow-hidden bg-background p-6"
  >
    <DotMatrixBg class="pointer-events-none absolute inset-0" />

    <section
      class="login-scope relative flex w-full max-w-[22rem] -translate-y-8 flex-col items-center gap-6 transition-[opacity,transform] ease-out motion-reduce:transition-none"
      :class="[
        exiting
          ? 'scale-[0.88] opacity-0 duration-[175ms] motion-reduce:scale-100 motion-reduce:opacity-100'
          : 'scale-100 opacity-100 duration-[450ms]',
        entering && 'scale-[1.15] opacity-0 motion-reduce:scale-100 motion-reduce:opacity-100',
      ]"
    >
      <div class="flex flex-col items-center gap-3 text-center">
        <img
          src="/logo.svg"
          class="size-14"
          alt=""
          aria-hidden="true"
        >
        <h1 class="text-display font-semibold text-foreground">
          {{ $t('auth.pageTitle') }}
        </h1>
        <p class="max-w-[18rem] text-sm text-muted-foreground">
          {{ $t('auth.pageSubtitle') }}
        </p>
      </div>

      <!-- 两字段 + Continue 等距 gap-2.5(不学 SaaS 密码步拉开 CTA)。
           placeholder 用 Enter …(P0 label≠placeholder)。 -->
      <form
        v-if="!loginSucceeded"
        class="flex w-full flex-col gap-2.5"
        @submit.prevent="login"
      >
        <Input
          id="username"
          v-model="username"
          type="text"
          :placeholder="$t('auth.usernamePlaceholder')"
          :disabled="isSubmitting"
          autocomplete="username"
          autofocus
        />
        <PasswordInput
          id="password"
          v-model="password"
          :placeholder="$t('auth.passwordPlaceholder')"
          autocomplete="current-password"
          :disabled="isSubmitting"
        />
        <LoadingButton
          type="submit"
          block
          :loading="isSubmitting"
          :loading-delay="0"
          :disabled="!canSubmit"
        >
          {{ $t('auth.continue') }}
        </LoadingButton>
      </form>
      <div
        v-else
        class="flex w-full flex-col items-center gap-3"
      >
        <CircleCheck class="size-12 text-foreground" />
      </div>
    </section>
  </main>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Input } from '@felinic/ui'
import { CircleCheck } from 'lucide-vue-next'
import { useRouter } from 'vue-router'
import { useUserStore } from '@/store/user'
import { toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { postAuthLogin } from '@memohai/sdk'
import LoadingButton from '@/components/loading-button/index.vue'
import PasswordInput from '@/components/password-input/index.vue'
import DotMatrixBg from '@/pages/login/components/dot-matrix-bg.vue'
import { submitLogin } from './login-submit'
import { safeSessionGet, safeSessionRemove, safeSessionSet } from '@/utils/safe-storage'
import { ONBOARDING_KEYS } from '@/pages/onboarding/constants'
import { LOGIN_ENTRY_ANIMATION_KEY } from './transition'

const router = useRouter()
const { t } = useI18n()

const username = ref('')
const password = ref('')
const loginSucceeded = ref(false)
const exiting = ref(false)
const shouldAnimateEntry = safeSessionGet(LOGIN_ENTRY_ANIMATION_KEY) === '1'
if (shouldAnimateEntry) safeSessionRemove(LOGIN_ENTRY_ANIMATION_KEY)
const entering = ref(shouldAnimateEntry)

onMounted(() => {
  if (!shouldAnimateEntry) return
  requestAnimationFrame(() => {
    requestAnimationFrame(() => {
      entering.value = false
    })
  })
})

const canSubmit = computed(() =>
  !!username.value.trim() && !!password.value.trim(),
)

const { login: loginHandle } = useUserStore()
const isSubmitting = ref(false)

const login = async () => {
  if (!canSubmit.value || isSubmitting.value) return
  await submitLogin(
    { username: username.value.trim(), password: password.value },
    isSubmitting,
    {
      authenticate: (body) => postAuthLogin({ body }),
      applyLogin: loginHandle,
      navigateHome: async () => {
        loginSucceeded.value = true
        await new Promise<void>(resolve => setTimeout(resolve, 700))
        exiting.value = true
        safeSessionSet(ONBOARDING_KEYS.entryAnimation, '1')
        await new Promise<void>(resolve => setTimeout(resolve, 175))
        await router.replace({ path: '/' })
      },
      notifyInvalidCredentials: () => {
        // 可显隐密码框:失败不清空,用户多半只改账号或笔误。
        toast.error(t('auth.invalidCredentials'), {
          description: t('auth.retryHint'),
        })
      },
    },
  )
}
</script>

<style scoped>
/* placeholder 是主要可见文案,与全局 placeholder(360)相比提半档。 */
.login-scope :deep(input)::placeholder {
  font-weight: 450;
}
</style>

<!-- 非 scoped:scoped 内 :global(.dark) 组合选择器编译不可靠;用 .login-scope 限定。 -->
<style>
/* ── 登录页专属控件语言(与 SaaS 登录同源)────────────────────
 * 浅色 rest = 纯白(非 #ececec 脏灰);暗色 #242424。
 * hover = 白纱盖在 content 上(图标色不动,整体被洗亮),0ms。 */
.login-scope {
  --login-control: #ffffff;
  --login-outline-hover-opacity: 0.68;
  --login-hover-duration: 10ms;
  --login-primary-hover: color-mix(in oklab, var(--btn-primary), #fff 18%);
  --login-primary-active: color-mix(in oklab, var(--btn-primary), #fff 28%);
  --login-primary-disabled: color-mix(in oklab, var(--btn-primary), #fff 42%);
}
.dark .login-scope {
  --login-control: #242424;
  --login-outline-hover-opacity: 0.72;
  --login-primary-hover: color-mix(in oklab, var(--btn-primary), #000 12%);
  --login-primary-active: color-mix(in oklab, var(--btn-primary), #000 20%);
  --login-primary-disabled: color-mix(in oklab, var(--btn-primary), #000 28%);
}

/* 防双击选中系统蓝框 */
.login-scope [data-button] {
  user-select: none;
  -webkit-user-select: none;
}

/* outline hover = 整颗降透明度;10ms */
.login-scope [data-button][data-variant="outline"]:not(:disabled) {
  transition: opacity var(--login-hover-duration) ease-out;
}
.login-scope [data-button][data-variant="outline"]:not(:disabled):hover,
.login-scope [data-button][data-variant="outline"]:not(:disabled):active {
  opacity: var(--login-outline-hover-opacity);
}

/* ::after 仅禁用纱 */
.login-scope [data-button]:not([data-variant="quiet"])::after {
  content: "";
  position: absolute;
  inset: 0;
  z-index: 1;
  border-radius: inherit;
  background-color: var(--background);
  opacity: 0;
  pointer-events: none;
  transition: opacity 0.2s ease-out;
}
.login-scope [data-button]:not([data-variant="quiet"]):disabled:not([data-loading])::after,
.login-scope [data-button]:not([data-variant="quiet"]):disabled:hover::after,
.login-scope [data-button]:not([data-variant="quiet"]):disabled:active::after {
  opacity: 0.32;
}
.login-scope [data-button]:disabled {
  opacity: 1;
  pointer-events: auto;
  cursor: not-allowed;
}

/* 浅色描边两档:输入框 field-edge-rest(≈11%);按钮再淡到 ≈7%(大块+字重同浓度更重) */
.login-scope {
  --login-edge-field: inset 0 0 0 1px var(--field-edge-rest);
  --login-edge-btn: inset 0 0 0 1px oklch(0 0 0 / 0.09);
}
.login-scope [data-slot="input"],
.login-scope [data-slot="input-group"] {
  background-color: var(--login-control);
  box-shadow: var(--login-edge-field);
  transition: background-color 0.2s ease-out, box-shadow 0.06s ease-out;
}
.dark .login-scope [data-slot="input"],
.dark .login-scope [data-slot="input-group"] {
  box-shadow: none;
}
.login-scope [data-slot="input"]:focus,
.login-scope [data-slot="input-group"]:focus-within {
  box-shadow: var(--login-edge-field);
}
.dark .login-scope [data-slot="input"]:focus,
.dark .login-scope [data-slot="input-group"]:focus-within {
  box-shadow: none;
}
.login-scope [data-slot="input"]:disabled,
.login-scope [data-slot="input-group"]:has(input:disabled) {
  opacity: 1;
  cursor: not-allowed;
}

/* 浏览器原生自动填充覆盖(与 SaaS 登录页 saas/login.vue 同源,发现于该页
 * 2026-07-09 的开发过程,详见其文件内注释):Chrome/Safari 对 autofill 的
 * <input> 会强制刷一层它自己的背景,且只认 -webkit-text-fill-color、不认
 * 普通 color——本页文字色是 --foreground(暗色下接近纯白),一旦浏览器把
 * 背景抢成浅色,白字就读不出来了。用 inset box-shadow 撑满整个输入框把
 * 浏览器的背景"挤"成看不见,再显式钉住文字色。
 * transition delay 用大数字拖延 -webkit 的显示时机,是该 hack 的标准写法。
 * ⚠ 与 saas/login.vue 保持同步:改这段前先看那边是否也要跟着改。
 * ⚠ 选择器同时覆盖 [data-slot="input"] 和 [data-slot="input-group-control"]:
 * 本页密码框走 PasswordInput → InputGroup → InputGroupInput,内层 <input>
 * 的 data-slot 被 InputGroupInput 显式传成了 "input-group-control"(Vue
 * fallthrough attrs 规则覆盖了 Input.vue 内部写死的 "input"),只写
 * [data-slot="input"] 会漏掉密码框——账号用 Input(data-slot="input")没事,
 * 密码这个最容易被浏览器自动填充命中的字段却选不中。
 * input-group-control 不带 --login-edge-field:那圈描边已经画在外层
 * [data-slot="input-group"] 容器上,内层再叠一份会形成双层描边。 */
.login-scope [data-slot="input"]:-webkit-autofill,
.login-scope [data-slot="input"]:-webkit-autofill:hover,
.login-scope [data-slot="input"]:-webkit-autofill:focus,
.login-scope [data-slot="input"]:-webkit-autofill:active {
  box-shadow: var(--login-edge-field), 0 0 0px 1000px var(--login-control) inset;
  -webkit-text-fill-color: var(--foreground);
  caret-color: var(--foreground);
  transition: background-color 99999s ease-in-out 0s;
}
.login-scope [data-slot="input-group-control"]:-webkit-autofill,
.login-scope [data-slot="input-group-control"]:-webkit-autofill:hover,
.login-scope [data-slot="input-group-control"]:-webkit-autofill:focus,
.login-scope [data-slot="input-group-control"]:-webkit-autofill:active {
  box-shadow: 0 0 0px 1000px var(--login-control) inset;
  -webkit-text-fill-color: var(--foreground);
  caret-color: var(--foreground);
  transition: background-color 99999s ease-in-out 0s;
}
.dark .login-scope [data-slot="input"]:-webkit-autofill,
.dark .login-scope [data-slot="input"]:-webkit-autofill:hover,
.dark .login-scope [data-slot="input"]:-webkit-autofill:focus,
.dark .login-scope [data-slot="input"]:-webkit-autofill:active,
.dark .login-scope [data-slot="input-group-control"]:-webkit-autofill,
.dark .login-scope [data-slot="input-group-control"]:-webkit-autofill:hover,
.dark .login-scope [data-slot="input-group-control"]:-webkit-autofill:focus,
.dark .login-scope [data-slot="input-group-control"]:-webkit-autofill:active {
  box-shadow: 0 0 0px 1000px var(--login-control) inset;
}

/* outline:描边 hover 不摘;fill 不动,靠整颗 opacity */
.login-scope [data-button][data-variant="outline"]::before,
.login-scope [data-button][data-variant="outline"]:not(:disabled):hover::before,
.login-scope [data-button][data-variant="outline"]:not(:disabled):active::before,
.login-scope [data-button][data-variant="outline"]:disabled:hover::before,
.login-scope [data-button][data-variant="outline"]:disabled:active::before {
  background-color: var(--login-control);
  box-shadow: var(--login-edge-btn);
}
.dark .login-scope [data-button][data-variant="outline"]::before,
.dark .login-scope [data-button][data-variant="outline"]:not(:disabled):hover::before,
.dark .login-scope [data-button][data-variant="outline"]:not(:disabled):active::before,
.dark .login-scope [data-button][data-variant="outline"]:disabled:hover::before,
.dark .login-scope [data-button][data-variant="outline"]:disabled:active::before {
  background-color: var(--login-control);
  box-shadow: none;
}

/* primary:轻抬 fill;10ms;不可点往白抬 */
.login-scope [data-button]:is([data-variant="default"], [data-variant="primary"])::before,
.dark .login-scope [data-button]:is([data-variant="default"], [data-variant="primary"])::before {
  transition: background-color var(--login-hover-duration) ease-out;
}
.login-scope [data-button]:is([data-variant="default"], [data-variant="primary"]):not(:disabled):hover::before,
.dark .login-scope [data-button]:is([data-variant="default"], [data-variant="primary"]):not(:disabled):hover::before {
  background-color: var(--login-primary-hover);
}
.login-scope [data-button]:is([data-variant="default"], [data-variant="primary"]):not(:disabled):active::before,
.login-scope [data-button]:is([data-variant="default"], [data-variant="primary"])[data-block]:not(:disabled):active::before,
.dark .login-scope [data-button]:is([data-variant="default"], [data-variant="primary"]):not(:disabled):active::before,
.dark .login-scope [data-button]:is([data-variant="default"], [data-variant="primary"])[data-block]:not(:disabled):active::before {
  background-color: var(--login-primary-active);
  transition: background-color var(--login-hover-duration) ease-out;
}
.login-scope [data-button]:is([data-variant="default"], [data-variant="primary"]):disabled:not([data-loading])::before,
.dark .login-scope [data-button]:is([data-variant="default"], [data-variant="primary"]):disabled:not([data-loading])::before {
  background-color: var(--login-primary-disabled);
}
</style>
