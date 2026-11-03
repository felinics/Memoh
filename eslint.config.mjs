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
      'apps/web/src/pages/home/components/dockview/panel-asset.vue',
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
