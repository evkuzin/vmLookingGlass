import { useQuery } from "@tanstack/react-query";

// The API is served from the same origin as the UI in production, and
// proxied by the dev server otherwise.
const API_PATH = "api/v1";

type Envelope<T> = {
  status: "success" | "error";
  data?: T;
  error?: string;
};

export async function fetchAPI<T>(path: string, params?: URLSearchParams): Promise<T> {
  const query = params && [...params].length ? `?${params}` : "";
  const res = await fetch(`${API_PATH}/${path}${query}`);
  const body: Envelope<T> = await res.json().catch(() => ({
    status: "error" as const,
    error: `${res.status} ${res.statusText}`,
  }));

  if (body.status !== "success" || body.data === undefined) {
    throw new Error(body.error ?? `${res.status} ${res.statusText}`);
  }
  return body.data;
}

// useAPI keeps a query keyed on its path and parameters, so changing a filter
// refetches while an unchanged one is served from cache.
export function useAPI<T>(
  key: unknown[],
  path: string,
  params?: URLSearchParams,
  options?: { enabled?: boolean; refetchInterval?: number },
) {
  return useQuery<T, Error>({
    queryKey: [path, ...key],
    queryFn: () => fetchAPI<T>(path, params),
    enabled: options?.enabled ?? true,
    refetchInterval: options?.refetchInterval,
  });
}
