import js from '@eslint/js';
import { defineConfig, globalIgnores } from 'eslint/config';
import reactHooks from 'eslint-plugin-react-hooks';
import globals from 'globals';
import tseslint from 'typescript-eslint';

export default defineConfig([
  globalIgnores(['dist*', 'test-results', 'lint', 'codegen']),
  js.configs.recommended,
  tseslint.configs.recommended,
  reactHooks.configs.flat.recommended,
  { files: ['src/**/*.{ts,tsx}'], languageOptions: { globals: globals.browser } },
  { files: ['vite.config.ts', 'src/bundle.test.ts'], languageOptions: { globals: globals.node } },
  { files: ['e2e/**'], languageOptions: { globals: { ...globals.node, ...globals.browser } } },
]);
