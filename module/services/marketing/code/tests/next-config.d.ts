// next.config.mjs is JavaScript, and this project compiles with allowJs disabled, so
// the configuration carries no types of its own. The tests read the real file rather
// than a copy of its values — a test that restates the headers it is checking proves
// only that two literals agree — so the shape it exposes is declared here.
declare module "@/../next.config.mjs" {
  export type MarketingHeader = { key: string; value: string };
  export type MarketingHeaderGroup = { source: string; headers: MarketingHeader[] };
  const config: {
    headers?: () => Promise<MarketingHeaderGroup[]>;
  };
  export default config;
}
