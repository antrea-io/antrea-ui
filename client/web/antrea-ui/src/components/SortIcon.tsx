import React from 'react';

interface SortIconProps {
    direction?: 'ascending' | 'descending';
    active?: boolean;
}

export function SortIcon({ direction, active = false }: SortIconProps) {
    const opacity = active ? 1 : 0.3;
    
    return (
        <svg 
            width="16" 
            height="16" 
            viewBox="0 0 24 24" 
            style={{ opacity }}
        >
            <g stroke="none" strokeWidth="1" fill="none" fillRule="evenodd">
                {direction === 'ascending' ? (
                    <>
                        <rect x="4" y="4" width="16" height="3" rx="1" fill="currentColor"/>
                        <rect x="4" y="9" width="12" height="3" rx="1" fill="currentColor"/>
                        <rect x="4" y="14" width="8" height="3" rx="1" fill="currentColor"/>
                        <rect x="4" y="19" width="4" height="3" rx="1" fill="currentColor"/>
                    </>
                ) : (
                    <>
                        <rect x="4" y="4" width="4" height="3" rx="1" fill="currentColor"/>
                        <rect x="4" y="9" width="8" height="3" rx="1" fill="currentColor"/>
                        <rect x="4" y="14" width="12" height="3" rx="1" fill="currentColor"/>
                        <rect x="4" y="19" width="16" height="3" rx="1" fill="currentColor"/>
                    </>
                )}
            </g>
        </svg>
    );
} 