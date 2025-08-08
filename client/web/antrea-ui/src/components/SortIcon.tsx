import React from 'react';

interface SortIconProps {
    direction?: 'ascending' | 'descending';
    active?: boolean;
}

export function SortIcon({ direction = 'ascending', active = false }: SortIconProps) {
    if (!active) {
        // Show a neutral sort icon when not active
        return (
            <svg 
                width="14" 
                height="14" 
                viewBox="0 0 24 24" 
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeLinejoin="round"
                style={{ opacity: 0.3 }}
                aria-label="Sortable column"
            >
                <path d="M8 9l4-4 4 4" />
                <path d="M16 15l-4 4-4-4" />
            </svg>
        );
    }

    // Show directional arrow when active
    return (
        <svg 
            width="14" 
            height="14" 
            viewBox="0 0 24 24" 
            fill="none"
            stroke="currentColor"
            strokeWidth="2.5"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-label={`Sorted ${direction}`}
        >
            {direction === 'ascending' ? (
                <>
                    <path d="M12 19V5" />
                    <path d="M5 12l7-7 7 7" />
                </>
            ) : (
                <>
                    <path d="M12 5v14" />
                    <path d="M19 12l-7 7-7-7" />
                </>
            )}
        </svg>
    );
}
