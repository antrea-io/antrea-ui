/**
 * Copyright 2024 Antrea Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import js from '@eslint/js'
import globals from 'globals'
import react from 'eslint-plugin-react'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import vitest from "eslint-plugin-vitest";

export default tseslint.config(
    { ignores: ['dist'] },
    {
        extends: [js.configs.recommended, ...tseslint.configs.recommended],
        languageOptions: {
            ecmaVersion: 'latest',
            globals: globals.browser,
        },
        settings: { react: { version: 'detect' } },
        plugins: {
            'react': react,
            'react-hooks': reactHooks,
            'react-refresh': reactRefresh,
            'vitest': vitest,
        },
        rules: {
            ...react.configs.recommended.rules,
            ...react.configs['jsx-runtime'].rules,
            ...reactHooks.configs.recommended.rules,
            ...vitest.configs.recommended.rules,
            // for Clarity (cds properties)
            'react/no-unknown-property': ['off'],
            'react-refresh/only-export-components': [
                'warn',
                { allowConstantExport: true },
            ],
            '@typescript-eslint/no-unused-vars': [
                'error',
                { 'argsIgnorePattern': '^_' },
            ],
            '@typescript-eslint/no-explicit-any': [
                'error',
                { 'ignoreRestArgs': true },
            ],
            semi: ['warn', 'always'],
        },
    },
    // file-pattern specific overrides
    {
        files: ["**/*.test.tsx"],
        rules: {
            "@typescript-eslint/no-explicit-any": ["off"],
        },
    },
)
