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

// JSX type declarations for Antrea Lit Web Components.
// Allows using <antrea-button>, <antrea-input>, etc. in TSX files without
// TypeScript errors, and provides prop type checking.

import type { AntreaButton, AntreaAlert, AntreaCard, AntreaNav, AntreaNavItem, AntreaInput } from '@antrea/ui-components';
import React from 'react';

type ButtonAction = 'solid' | 'outline' | 'flat';
type AlertStatus = 'info' | 'success' | 'warning' | 'danger' | 'loading';
type InputType = 'text' | 'password' | 'email' | 'number';

declare global {
    namespace React.JSX {
        interface IntrinsicElements {
            'antrea-button': React.HTMLAttributes<AntreaButton> & {
                action?: ButtonAction;
                disabled?: boolean;
                type?: 'button' | 'submit' | 'reset';
            };
            'antrea-alert': React.HTMLAttributes<AntreaAlert> & {
                status?: AlertStatus;
                closable?: boolean;
                onAntreaClose?: (event: Event) => void;
            };
            'antrea-card': React.HTMLAttributes<AntreaCard> & {
                heading?: string;
            };
            'antrea-nav': React.HTMLAttributes<AntreaNav> & {
                expanded?: boolean | string;
            };
            'antrea-nav-item': React.HTMLAttributes<AntreaNavItem> & {
                active?: boolean;
            };
            'antrea-input': React.HTMLAttributes<AntreaInput> & {
                ref?: React.Ref<HTMLElement & { value: string }>;
                label?: string;
                placeholder?: string;
                value?: string;
                disabled?: boolean;
                error?: boolean;
                'error-message'?: string;
                type?: InputType;
                name?: string;
            };
            'antrea-summary-page': React.HTMLAttributes<HTMLElement> & React.ClassAttributes<HTMLElement> & {
                token?: string;
            };
            'antrea-settings-page': React.HTMLAttributes<HTMLElement> & React.ClassAttributes<HTMLElement> & {
                token?: string;
            };
            'antrea-traceflow-page': React.HTMLAttributes<HTMLElement> & React.ClassAttributes<HTMLElement> & {
                token?: string;
            };
            'antrea-flow-visibility-page': React.HTMLAttributes<HTMLElement> & React.ClassAttributes<HTMLElement> & {
                token?: string;
            };
        }
    }
}
