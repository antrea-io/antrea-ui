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

import { afterEach, describe, expect, test } from 'vitest';
import { LitElement } from 'lit';
import { renderStaticTable } from './render-table';

class TestTableHost extends LitElement {
    headers: string[] = [];
    rows: { name: string; value: string }[] = [];

    override createRenderRoot() {
        // Render into light DOM so plain DOM queries can inspect the output.
        return this;
    }

    override render() {
        return renderStaticTable(this.headers, this.rows, r => [r.name, r.value]);
    }
}
customElements.define('test-table-host', TestTableHost);

let el: TestTableHost;

afterEach(() => {
    el?.remove();
});

async function mount(headers: string[], rows: { name: string; value: string }[]): Promise<TestTableHost> {
    el = document.createElement('test-table-host') as TestTableHost;
    el.headers = headers;
    el.rows = rows;
    document.body.appendChild(el);
    await el.updateComplete;
    return el;
}

describe('renderStaticTable', () => {
    test('renders one header cell per header', async () => {
        const host = await mount(['Name', 'Value'], []);
        const headerCells = host.querySelectorAll('thead th');
        expect(Array.from(headerCells).map(c => c.textContent)).toEqual(['Name', 'Value']);
    });

    test('renders one row per data item, with cells from getRow', async () => {
        const host = await mount(['Name', 'Value'], [
            { name: 'a', value: '1' },
            { name: 'b', value: '2' },
        ]);
        const rows = host.querySelectorAll('tbody tr');
        expect(rows).toHaveLength(2);
        expect(Array.from(rows[0].querySelectorAll('td')).map(c => c.textContent)).toEqual(['a', '1']);
        expect(Array.from(rows[1].querySelectorAll('td')).map(c => c.textContent)).toEqual(['b', '2']);
    });

    test('renders an empty tbody when there are no rows', async () => {
        const host = await mount(['Name', 'Value'], []);
        expect(host.querySelectorAll('tbody tr')).toHaveLength(0);
    });
});
