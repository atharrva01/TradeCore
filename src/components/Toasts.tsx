import { X } from "lucide-react";
import { useToasts } from "../live/toasts";

export function Toasts() {
  const { toasts, dismiss } = useToasts();
  return (
    <div className="toast-stack" role="status" aria-live="polite">
      {toasts.map((item) => (
        <div key={item.id} className={`toast toast-${item.kind}`}>
          <span>{item.text}</span>
          <button title="Dismiss" onClick={() => dismiss(item.id)}>
            <X size={14} />
          </button>
        </div>
      ))}
    </div>
  );
}
