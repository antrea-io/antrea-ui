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

import { html } from 'lit';

/**
 * Renders a static (non-sortable) `.data-table`, given column headers, rows,
 * and a function that turns each row into its cell values. For a page that
 * needs sortable/interactive headers, don't use this — see
 * antrea-flow-visibility-page.ts's own table for that shape instead.
 */
export function renderStaticTable<T>(
    headers: string[],
    rows: T[],
    getRow: (item: T) => string[],
) {
    return html`
        <table class="data-table" part="table">
            <thead><tr>${headers.map(h => html`<th part="table-header-cell">${h}</th>`)}</tr></thead>
            <tbody>
                ${rows.map(item => html`
                    <tr>${getRow(item).map(v => html`<td part="table-cell">${v}</td>`)}</tr>
                `)}
            </tbody>
        </table>
    `;
}
