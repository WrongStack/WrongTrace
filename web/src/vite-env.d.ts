/// <reference types="vite/client" />

// Side-effect style imports (import './index.css') carry no type information;
// this ambient module declaration keeps them valid under noEmit type checks.
// TS 7 (Go-based tsc) started rejecting such imports (TS2882) where TS 5
// tolerated them, hence the explicit declaration.
declare module '*.css';
