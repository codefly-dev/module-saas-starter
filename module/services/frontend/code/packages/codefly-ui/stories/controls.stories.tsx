import { useState } from "react";
import {
	Button,
	CardRoot,
	CardHeader,
	CardTitle,
	CardDescription,
	CardAction,
	CardContent,
	CardFooter,
	TabsRoot,
	TabsList,
	TabsTrigger,
	TabsContent,
	Tabs,
	Command,
	CommandInput,
	CommandList,
	CommandItem,
	CommandEmpty,
	CommandGroup,
	InputGroup,
	InputGroupInput,
	InputGroupAddon,
	InputGroupButton,
	Sheet,
	SheetTrigger,
	SheetContent,
	SheetHeader,
	SheetTitle,
	SheetDescription,
	SidebarProvider,
	Sidebar,
	SidebarHeader,
	SidebarContent,
	SidebarMenu,
	SidebarMenuItem,
	SidebarMenuButton,
	SidebarTrigger,
	SidebarInset,
	Toaster,
	Layout,
	Page,
	PageHeader,
	Grid,
	Section,
	Panel,
	Stack,
} from "../src/layout/index.js";
import { toast } from "sonner";

export default { title: "Shared UI/Controls" };

export const CompoundCard = {
	render: () => (
		<CardRoot>
			<CardHeader>
				<CardTitle>Example workspace</CardTitle>
				<CardDescription>Members and settings</CardDescription>
				<CardAction>
					<Button variant="outline">Manage</Button>
				</CardAction>
			</CardHeader>
			<CardContent>Three active members</CardContent>
			<CardFooter>Updated today</CardFooter>
		</CardRoot>
	),
};
export const CompactCard = {
	render: () => (
		<CardRoot size="sm">
			<CardHeader>
				<CardTitle>Usage</CardTitle>
			</CardHeader>
			<CardContent>42 requests</CardContent>
		</CardRoot>
	),
};

function CompoundTabs({ vertical = false }: { vertical?: boolean }) {
	return (
		<TabsRoot
			defaultValue="overview"
			orientation={vertical ? "vertical" : "horizontal"}
		>
			<TabsList>
				<TabsTrigger value="overview">Overview</TabsTrigger>
				<TabsTrigger value="settings">Settings</TabsTrigger>
				<TabsTrigger value="disabled" disabled>
					Unavailable
				</TabsTrigger>
			</TabsList>
			<TabsContent value="overview">Workspace overview</TabsContent>
			<TabsContent value="settings" keepMounted>
				<label>
					Draft name
					<input defaultValue="Acme" />
				</label>
			</TabsContent>
		</TabsRoot>
	);
}
export const HorizontalTabs = { render: () => <CompoundTabs /> };
export const VerticalTabs = { render: () => <CompoundTabs vertical /> };
export const ControlledTabs = {
	render: function ControlledTabsStory() {
		const [active, setActive] = useState("one");
		return (
			<Tabs
				active={active}
				onChange={setActive}
				keepMounted
				tabs={[
					{ id: "one", label: "Overview", content: "Workspace overview" },
					{
						id: "two",
						label: "Draft",
						content: (
							<label>
								Draft
								<input defaultValue="Example" />
							</label>
						),
					},
				]}
			/>
		);
	},
};

export const SearchCommands = {
	render: () => (
		<Command>
			<CommandInput placeholder="Search actions" aria-label="Search actions" />
			<CommandList>
				<CommandEmpty>No matching actions.</CommandEmpty>
				<CommandGroup heading="Workspace">
					<CommandItem onSelect={() => toast.success("Selected settings")}>
						Settings
					</CommandItem>
					<CommandItem disabled>Delete workspace</CommandItem>
				</CommandGroup>
			</CommandList>
		</Command>
	),
};
export const InputWithAction = {
	render: () => (
		<InputGroup>
			<InputGroupAddon>https://</InputGroupAddon>
			<InputGroupInput aria-label="Website" placeholder="example.com" />
			<InputGroupAddon align="inline-end">
				<InputGroupButton>Open</InputGroupButton>
			</InputGroupAddon>
		</InputGroup>
	),
};
export const InvalidInput = {
	render: () => (
		<div>
			<label htmlFor="example-address">Website</label>
			<InputGroup>
				<InputGroupInput
					id="example-address"
					aria-invalid
					aria-describedby="example-address-error"
					defaultValue="invalid address"
				/>
			</InputGroup>
			<p id="example-address-error">Enter a valid website address.</p>
		</div>
	),
};
export const DisabledInput = {
	render: () => (
		<InputGroup>
			<InputGroupInput aria-label="Website" disabled value="example.com" />
		</InputGroup>
	),
};
export const SheetDialog = {
	render: () => (
		<Sheet>
			<SheetTrigger render={<Button />}>Open details</SheetTrigger>
			<SheetContent>
				<SheetHeader>
					<SheetTitle>Workspace details</SheetTitle>
					<SheetDescription>
						Review this workspace before continuing.
					</SheetDescription>
				</SheetHeader>
				<label>
					Workspace name
					<input defaultValue="Acme" />
				</label>
			</SheetContent>
		</Sheet>
	),
};
export const ResponsiveSidebar = {
	render: () => (
		<SidebarProvider>
			<Sidebar>
				<SidebarHeader>Example workspace</SidebarHeader>
				<SidebarContent>
					<SidebarMenu>
						<SidebarMenuItem>
							<SidebarMenuButton isActive>Overview</SidebarMenuButton>
						</SidebarMenuItem>
						<SidebarMenuItem>
							<SidebarMenuButton disabled>Unavailable</SidebarMenuButton>
						</SidebarMenuItem>
					</SidebarMenu>
				</SidebarContent>
			</Sidebar>
			<SidebarInset>
				<SidebarTrigger />
				<p>Workspace content</p>
			</SidebarInset>
		</SidebarProvider>
	),
};
export const ToastStates = {
	render: () => (
		<>
			<Toaster />
			<Stack direction="row">
				<Button onClick={() => toast.success("Changes saved")}>Success</Button>
				<Button onClick={() => toast.error("Unable to save changes")}>
					Error
				</Button>
				<Button
					onClick={() => toast.loading("Saving changes", { duration: 1500 })}
				>
					Loading
				</Button>
			</Stack>
		</>
	),
};
export const PageComposition = {
	render: () => (
		<Layout>
			<Page>
				<PageHeader
					title="Example workspace"
					description="Manage shared resources"
					actions={<Button>Create resource</Button>}
				/>
				<Section title="Resources">
					<Grid cols={2}>
						<Panel>First resource</Panel>
						<Panel>Second resource</Panel>
					</Grid>
				</Section>
			</Page>
		</Layout>
	),
};
