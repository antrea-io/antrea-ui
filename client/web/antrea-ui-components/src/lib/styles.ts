// Copyright 2026 Antrea Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { css } from 'lit';

/** Common styles shared across all page-level Lit components. */
export const pageStyles = css`
    :host {
        display: block;
        color: var(--antrea-color-text, #e9ecef);
        font-family: var(--antrea-font-family, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif);
        font-size: var(--antrea-font-size-base, 0.875rem);
    }

    main {
        padding: var(--antrea-space-md, 1rem);
    }

    .page-layout {
        display: flex;
        flex-direction: column;
        gap: 1.5rem;
    }

    .page-title {
        margin: 0;
        font-size: var(--antrea-font-size-heading, 1.25rem);
        font-weight: var(--antrea-font-weight-bold, 600);
        color: var(--antrea-color-text, #e9ecef);
    }

    .row {
        display: flex;
        align-items: center;
        gap: 1rem;
    }

    .btn-group {
        display: flex;
        gap: 0.25rem;
    }

    /* ── Tables ──────────────────────────────────────────── */
    .data-table {
        width: 100%;
        border-collapse: collapse;
        font-size: var(--antrea-font-size-base, 0.875rem);
        color: var(--antrea-color-text, #e9ecef);
    }
    .data-table th,
    .data-table td {
        padding: var(--antrea-space-xs, 0.25rem) var(--antrea-space-sm, 0.5rem);
        border: 1px solid var(--antrea-color-border, #314351);
        text-align: center;
        letter-spacing: var(--antrea-table-cell-letter-spacing, normal);
    }
    .data-table th {
        background: var(--antrea-color-bg-active, #375060);
        font-weight: var(--antrea-table-header-font-weight, var(--antrea-font-weight-medium, 500));
        text-transform: var(--antrea-table-header-text-transform, none);
        letter-spacing: var(--antrea-table-header-letter-spacing, normal);
        white-space: nowrap;
    }
    /* Only headers that actually implement click-to-sort should look clickable. */
    .data-table th.sortable {
        cursor: pointer;
        user-select: none;
    }
    .data-table th.sortable:hover { background: var(--antrea-color-bg-hover, #2e3f4d); }

    /* ── Forms ───────────────────────────────────────────── */
    .form-stack {
        display: flex;
        flex-direction: column;
        gap: var(--antrea-space-md, 1rem);
        max-width: 480px;
    }

    .field-group {
        display: flex;
        flex-direction: column;
        gap: var(--antrea-space-xs, 0.25rem);
    }

    .field-label {
        font-size: var(--antrea-font-size-sm, 0.75rem);
        font-weight: var(--antrea-font-weight-medium, 500);
        color: var(--antrea-color-text-muted, #adbbc4);
    }

    .field-hint {
        font-size: var(--antrea-font-size-sm, 0.75rem);
        color: var(--antrea-color-text-muted, #adbbc4);
    }

    .field-input,
    .field-select {
        padding: 0.375rem 0.625rem;
        background: var(--antrea-color-bg, #1b2a32);
        border: 1px solid var(--antrea-color-border, #314351);
        border-radius: var(--antrea-radius-md, 4px);
        color: var(--antrea-color-text, #e9ecef);
        font-size: var(--antrea-font-size-base, 0.875rem);
        font-family: inherit;
        width: 100%;
        box-sizing: border-box;
    }
    .field-input:focus,
    .field-select:focus {
        outline: 2px solid var(--antrea-color-border-focus, #0079b8);
        outline-offset: 1px;
        border-color: var(--antrea-color-border-focus, #0079b8);
    }
    .field-input.error { border-color: var(--antrea-color-danger, #f54f47); }

    .field-select {
        appearance: none;
        cursor: pointer;
        padding-right: 2rem;
        background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 16 16'%3E%3Cpath fill='%23adbbc4' d='M7.247 11.14L2.451 5.658C1.885 5.013 2.345 4 3.204 4h9.592a1 1 0 0 1 .753 1.659l-4.796 5.48a1 1 0 0 1-1.506 0z'/%3E%3C/svg%3E");
        background-repeat: no-repeat;
        background-position: right 0.625rem center;
    }

    .field-error {
        font-size: var(--antrea-font-size-sm, 0.75rem);
        color: var(--antrea-color-danger, #f54f47);
    }

    .checkbox-label,
    .radio-label {
        display: inline-flex;
        align-items: center;
        gap: var(--antrea-space-xs, 0.25rem);
        font-size: var(--antrea-font-size-base, 0.875rem);
        color: var(--antrea-color-text, #e9ecef);
        cursor: pointer;
    }

    .radio-group {
        display: flex;
        gap: var(--antrea-space-md, 1rem);
        flex-wrap: wrap;
    }

    /* ── Spinner ─────────────────────────────────────────── */
    .spinner {
        display: inline-block;
        width: 2rem;
        height: 2rem;
        border: 3px solid var(--antrea-color-border, #314351);
        border-top-color: var(--antrea-color-primary, #0079b8);
        border-radius: 50%;
        animation: spin 0.8s linear infinite;
        flex-shrink: 0;
    }
    @keyframes spin { to { transform: rotate(360deg); } }

    .loading-row {
        display: flex;
        align-items: center;
        gap: var(--antrea-space-md, 1rem);
    }

    .text-muted {
        color: var(--antrea-color-text-muted, #adbbc4);
        font-size: var(--antrea-font-size-sm, 0.75rem);
    }
`;
