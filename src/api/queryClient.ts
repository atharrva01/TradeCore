import { QueryClient } from "@tanstack/react-query";
import { ApiError } from "./client";

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      refetchOnWindowFocus: false,
      // Keep retrying while a sleeping server wakes up, but not on client errors.
      retry: (failures, error) =>
        error instanceof ApiError && error.status >= 400 && error.status < 500 ? false : failures < 20,
      retryDelay: (attempt) => Math.min(1_000 * 2 ** attempt, 8_000),
    },
  },
});
