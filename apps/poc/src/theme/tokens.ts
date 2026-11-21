// The colour tokens, light and dark: the one source for the stylesheet's
// custom properties. contrast.test.ts holds the pairs to WCAG AA.
export type Tokens = Record<string, string>;
export const tokens: { light: Tokens; dark: Tokens } = { light: {}, dark: {} };
