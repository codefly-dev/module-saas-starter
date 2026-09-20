// The font catalog: every open face a skin may name in `fontSans`,
// `fontHeading` or `fontMono`, self-hosted from the package tree so a skin's
// family resolves offline and identically on every deployment. A skin names a
// family; this is what makes the name mean something. A face missing here
// falls through to the next entry of the skin's own stack, which is why every
// skin stack ends in a generic family.
//
// Side-effect imports only: each registers @font-face rules for one weight and
// nothing else. Kept to the weights the type roles can ask for (400–700).
import "@fontsource/figtree/400.css";
import "@fontsource/figtree/500.css";
import "@fontsource/figtree/600.css";
import "@fontsource/figtree/700.css";
import "@fontsource/inter/400.css";
import "@fontsource/inter/500.css";
import "@fontsource/inter/600.css";
import "@fontsource/inter/700.css";
import "@fontsource/jetbrains-mono/400.css";
import "@fontsource/jetbrains-mono/500.css";
import "@fontsource/newsreader/400.css";
import "@fontsource/newsreader/500.css";
import "@fontsource/newsreader/600.css";
import "@fontsource/roboto/400.css";
import "@fontsource/roboto/500.css";
import "@fontsource/roboto/700.css";
import "@fontsource/source-serif-4/400.css";
import "@fontsource/source-serif-4/600.css";

/** Families the catalog registers, for the docs and the test that holds them together. */
export const FONT_CATALOG = [
	"Figtree",
	"Inter",
	"JetBrains Mono",
	"Newsreader",
	"Roboto",
	"Source Serif 4",
] as const;
