import type { ComponentType, LazyExoticComponent } from "react";

export interface Plugin {
  id: string;
  name: string;
  routes: RouteDefinition[];
  navItems: NavItem[];
  providers?: ComponentType<{ children: React.ReactNode }>;
  widgets?: DashboardWidget[];
}

export interface RouteDefinition {
  path: string;
  component: LazyExoticComponent<ComponentType>;
  permissions?: string[];
  layout?: "dashboard" | "fullscreen" | "auth";
}

export interface NavItem {
  label: string;
  href: string;
  icon?: ComponentType;
  section?: string;
}

export interface DashboardWidget {
  id: string;
  component: LazyExoticComponent<ComponentType>;
  priority?: number;
}
