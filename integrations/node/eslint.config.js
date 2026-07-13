import { defineConfig } from '@entwico/eslint-config';
import globals from 'globals';

export default defineConfig({
  root: import.meta.dirname,
  extra: [
    {
      files: ['**/*.{js,mjs}'],
      languageOptions: { globals: globals.nodeBuiltin },
    },
    {
      files: ['**/*.cjs'],
      languageOptions: { sourceType: 'commonjs', globals: globals.node },
      rules: {
        '@typescript-eslint/no-require-imports': 'off',
        // cjs has no top-level await; a trailing main().catch() is the entrypoint idiom
        'unicorn/prefer-await': 'off',
      },
    },
  ],
});
