/**
 * Auth layout — NO sidebar, NO nav. Just a centered card on a clean background.
 * Used for: /auth/login, /auth/callback, /auth/magic-link
 */
export default function AuthLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="min-h-screen flex items-center justify-center bg-background">
      {children}
    </div>
  );
}
