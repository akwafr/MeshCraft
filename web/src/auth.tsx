import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import { get, post, type User } from "./api";

interface AuthState {
  user: User | null;
  loading: boolean;
  login: (email: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}

const AuthContext = createContext<AuthState>({
  user: null,
  loading: true,
  login: async () => {},
  logout: async () => {},
});

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    get<User>("/api/auth/me")
      .then(setUser)
      .catch(() => setUser(null))
      .finally(() => setLoading(false));
    const onUnauthorized = () => setUser(null);
    window.addEventListener("meshcraft:unauthorized", onUnauthorized);
    return () =>
      window.removeEventListener("meshcraft:unauthorized", onUnauthorized);
  }, []);

  const login = useCallback(async (email: string, password: string) => {
    const u = await post<User>("/api/auth/login", { email, password });
    setUser(u);
  }, []);

  const logout = useCallback(async () => {
    await post("/api/auth/logout");
    setUser(null);
  }, []);

  return (
    <AuthContext.Provider value={{ user, loading, login, logout }}>
      {children}
    </AuthContext.Provider>
  );
}

export const useAuth = () => useContext(AuthContext);

/**
 * Sites where the user is site_admin. Used to bound what they can do on a
 * proxy: touch only their sites (see docs/01-Architecture/Proxy). A
 * global-admin is unaffected — they act everywhere.
 */
export function useAdminSiteIDs(): string[] {
  const { user } = useAuth();
  return (user?.roles ?? []).filter((r) => r.role === "site_admin").map((r) => r.site_id);
}

/**
 * Effective role on a site: "ing" (engineer = site_admin or global admin,
 * full configuration), "tech" (technician = operator, applies profiles),
 * "viewer" (read-only).
 */
export function useSiteRole(siteId?: string): "ing" | "tech" | "viewer" {
  const { user } = useAuth();
  if (!user) return "viewer";
  if (user.is_global_admin) return "ing";
  const role = user.roles?.find((r) => r.site_id === siteId)?.role;
  if (role === "site_admin") return "ing";
  if (role === "operator") return "tech";
  return "viewer";
}
