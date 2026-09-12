import {
	Button,
	Input,
	Textarea,
	Label,
	Checkbox,
	Switch,
	Select,
	SelectTrigger,
	SelectValue,
	SelectContent,
	SelectItem,
	Badge,
	Avatar,
	AvatarImage,
	AvatarFallback,
	Skeleton,
	Separator,
	Dialog,
	DialogTrigger,
	DialogContent,
	DialogHeader,
	DialogTitle,
	DialogDescription,
	DialogFooter,
	DialogClose,
	AlertDialog,
	AlertDialogTrigger,
	AlertDialogContent,
	AlertDialogHeader,
	AlertDialogTitle,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogCancel,
	AlertDialogAction,
	Tooltip,
	TooltipTrigger,
	TooltipContent,
	DropdownMenu,
	DropdownMenuTrigger,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	EmptyState,
	ErrorState,
} from "../src/layout/index.js";

export default { title: "Shared UI/Primitives" };
export const Buttons = {
	render: () => (
		<div>
			<Button>Save</Button>
			<Button variant="outline">Cancel</Button>
			<Button variant="destructive">Delete</Button>
			<Button disabled>Unavailable</Button>
			<Button disabled aria-busy>
				Saving…
			</Button>
		</div>
	),
};
export const Fields = {
	render: () => (
		<div>
			<Label htmlFor="name">Name</Label>
			<Input id="name" placeholder="Jane Doe" />
			<Label htmlFor="notes">Notes</Label>
			<Textarea id="notes" />
			<Input aria-label="Read only" readOnly value="Acme" />
			<Input aria-label="Disabled" disabled />
			<Input
				aria-label="Invalid email"
				aria-invalid
				aria-describedby="email-error"
				defaultValue="invalid"
			/>
			<p id="email-error">Enter a valid email address.</p>
		</div>
	),
};
export const CheckboxesAndSwitches = {
	render: () => (
		<div>
			<label>
				<Checkbox defaultChecked />
				Notifications
			</label>
			<label>
				<Checkbox disabled />
				Unavailable
			</label>
			<label>
				<Switch defaultChecked />
				Email updates
			</label>
			<label>
				<Switch disabled />
				Unavailable updates
			</label>
		</div>
	),
};
export const Selection = {
	render: () => (
		<Select defaultValue="one">
			<SelectTrigger aria-label="Workspace">
				<SelectValue />
			</SelectTrigger>
			<SelectContent>
				<SelectItem value="one">Acme</SelectItem>
				<SelectItem value="two">ExampleCorp</SelectItem>
				<SelectItem value="three" disabled>
					Unavailable
				</SelectItem>
			</SelectContent>
		</Select>
	),
};
export const BadgesAndAvatars = {
	render: () => (
		<div>
			<Badge>Active</Badge>
			<Badge variant="outline">Pending</Badge>
			<Badge variant="destructive">Failed</Badge>
			<Avatar>
				<AvatarImage
					src="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg'/%3E"
					alt="Jane Doe"
				/>
				<AvatarFallback>JD</AvatarFallback>
			</Avatar>
		</div>
	),
};
export const Loading = {
	render: () => (
		<div aria-label="Loading content" aria-busy>
			<Skeleton className="h-6 w-48" />
			<Separator />
			<Skeleton className="h-24 w-full" />
		</div>
	),
};
export const Modal = {
	render: () => (
		<Dialog>
			<DialogTrigger render={<Button />}>Edit workspace</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Edit workspace</DialogTitle>
					<DialogDescription>Update the workspace name.</DialogDescription>
				</DialogHeader>
				<Label htmlFor="workspace-name">Name</Label>
				<Input id="workspace-name" defaultValue="Acme" />
				<DialogFooter>
					<DialogClose render={<Button variant="outline" />}>
						Cancel
					</DialogClose>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	),
};
export const Confirmation = {
	render: () => (
		<AlertDialog>
			<AlertDialogTrigger render={<Button variant="destructive" />}>
				Delete example
			</AlertDialogTrigger>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>Delete example?</AlertDialogTitle>
					<AlertDialogDescription>
						This preview has no destructive operation attached.
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel>Cancel</AlertDialogCancel>
					<AlertDialogAction>Confirm</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	),
};
export const TooltipPreview = {
	render: () => (
		<Tooltip>
			<TooltipTrigger render={<Button />}>Help</TooltipTrigger>
			<TooltipContent>Workspace help</TooltipContent>
		</Tooltip>
	),
};
export const Menu = {
	render: () => (
		<DropdownMenu>
			<DropdownMenuTrigger render={<Button />}>Actions</DropdownMenuTrigger>
			<DropdownMenuContent>
				<DropdownMenuItem>View details</DropdownMenuItem>
				<DropdownMenuSeparator />
				<DropdownMenuItem disabled>Unavailable action</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	),
};
export const Empty = {
	render: () => (
		<EmptyState
			heading="No resources"
			description="Create a resource to get started."
		>
			<Button>Create resource</Button>
		</EmptyState>
	),
};
export const Error = {
	render: () => (
		<ErrorState
			title="Unable to load resources."
			detail="The data provider is unavailable."
		/>
	),
};
