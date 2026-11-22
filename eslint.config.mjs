// @ts-check
import vueParser from 'vue-eslint-parser'
import tseslint from 'typescript-eslint'
import vue from 'eslint-plugin-vue'

export default [
  ...tseslint.configs.recommended,
  ...vue.configs['flat/recommended'],
  { ignores: ['**/node_modules/**', '**/dist/**', '**/out/**', '**/cache/**', '**/target/**', '**/.toolkit/**', 'packages/sdk/src/**'] },
  {
    files: ['packages/**/*.{js,jsx,ts,tsx}', 'apps/**/*.{js,jsx,ts,tsx}'],
    languageOptions: {
      parserOptions: {
        ecmaVersion: 2022,
        sourceType: 'module',
        projectService: true,
      },
    },
    rules: {
      quotes: ['error', 'single'],
      semi: ['error', 'never'],
      '@typescript-eslint/no-unused-vars': ['error', {
        argsIgnorePattern: '^_',
        varsIgnorePattern: '^_',
        destructuredArrayIgnorePattern: '^_',
      }],
    },
  },
  {
    files: ['apps/web/src/store/**/*.ts'],
    ignores: [
      'apps/web/src/store/workspace-tabs.ts',
      'apps/web/src/store/**/*.test.ts',
    ],
    rules: {
      'max-lines': ['error', {
        max: 600,
        skipBlankLines: true,
        skipComments: true,
      }],
      'no-restricted-imports': ['error', {
        paths: [
          {
            name: '@/router',
            message: '违反 M6（保持分发边界）：共享 Pinia store 严禁直接导入 Web 专用的 @/router（Desktop 使用内存路由），请使用宿主注入的 router。',
          },
        ],
      }],
    },
  },
  {
    files: ['apps/web/src/**/*.{ts,tsx,vue}'],
    ignores: [
      'apps/web/src/**/*.test.ts',
      'apps/web/src/lib/api-client.ts',
      'apps/web/src/store/update.ts',
      'apps/web/src/components/provider-icon/preload.ts',
      'apps/web/src/components/sidebar/files-pane.vue',
      'apps/web/src/pages/home/components/attachment-block.vue',
      'apps/web/src/pages/home/components/browser-pane.vue',
    ],
    rules: {
      'no-restricted-globals': [
        'error',
        {
          name: 'fetch',
          message: '违反 M3（服务端数据只有一条访问路径）：禁止在业务代码直接调用裸 fetch()，请统一使用 @memohai/sdk 配合 Pinia Colada useQuery / useMutation。',
        },
      ],
      'no-restricted-imports': [
        'error',
        {
          paths: [
            {
              name: 'reka-ui',
              message: '违反 M1 / M2：业务层严禁直接导入 reka-ui，请使用 @felinic/ui 封装的组件原语。',
            },
          ],
        patterns: [
          {
            group: ['@fortawesome/*'],
            message: '违反 M1 / U9：严禁使用 FontAwesome 图标，通用图标请用 lucide-vue-next，品牌图标请用 @memohai/icon。',
          },
        ],
      },
    ],
  },
},
{
  files: ['apps/web/src/**/*.{ts,tsx,vue}', 'apps/desktop/src/renderer/**/*.{ts,tsx,vue}'],
  ignores: [
    'apps/web/src/**/*.test.ts',
    'apps/desktop/src/**/*.test.ts',
    'apps/web/src/vitest-setup.ts',
    'apps/web/src/lib/runtime-env.ts',
    'apps/web/src/composables/useEnvironment.ts',
  ],
  rules: {
    'no-restricted-syntax': [
      'error',
      {
        selector: "ImportSpecifier[imported.name='useMediaQuery']",
        message: '违反 M1 / M2 / U8：禁止在业务层直接调用 useMediaQuery，请通过 @/composables/useEnvironment 统一读取布局空间、输入方式或无障碍偏好。',
      },
      {
        selector: "CallExpression[callee.name='matchMedia']",
        message: '违反 M1 / M2 / U8：禁止直接调用裸 matchMedia，请统一使用 @/lib/runtime-env 或 @/composables/useEnvironment。',
      },
      {
        selector: "MemberExpression[property.name='matchMedia']",
        message: '违反 M1 / M2 / U8：禁止直接调用 window.matchMedia，请统一使用 @/lib/runtime-env 或 @/composables/useEnvironment。',
      },
      {
        selector: "MemberExpression[object.name='navigator'][property.name=/^(platform|userAgent|maxTouchPoints)$/]",
        message: '违反 M2 / M6 / U8：禁止直接读取 navigator.platform / userAgent / maxTouchPoints，请统一使用 @/lib/runtime-env 或 @/composables/useEnvironment。',
      },
      {
        selector: "MemberExpression[object.property.name='navigator'][property.name=/^(platform|userAgent|maxTouchPoints)$/]",
        message: '违反 M2 / M6 / U8：禁止直接读取 navigator.platform / userAgent / maxTouchPoints，请统一使用 @/lib/runtime-env 或 @/composables/useEnvironment。',
      },
    ],
  },
},
  {
    files: ['packages/**/*.vue', 'apps/**/*.vue'],
    languageOptions: {
      parser: vueParser,
      parserOptions: {
        ecmaVersion: 2022,
        sourceType: 'module',
        parser: {
          js: 'espree',
          ts: tseslint.parser,
        },
      },
    },
    rules: {
      quotes: ['error', 'single'],
      semi: ['error', 'never'],
      'vue/multi-word-component-names': 'off',
      'vue/require-default-prop': 'off',
      'vue/no-required-prop-with-default':'error',
      '@typescript-eslint/no-unused-vars': ['error', {
        argsIgnorePattern: '^_',
        varsIgnorePattern: '^_',
        destructuredArrayIgnorePattern: '^_',
      }],
    },
  },
]
