// Re-export of the kit primitive. The component now ships once from
// @codefly-dev/ui/layout (issue #451, sealed layers #450); this module keeps the
// `@/components/ui/…` import path stable for existing host callers.
export {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogMedia,
	AlertDialogOverlay,
	AlertDialogPortal,
	AlertDialogTitle,
	AlertDialogTrigger,
} from "@codefly-dev/ui/layout";
