import { Link } from "react-router-dom";

export function NotFound() {
  return (
    <div className="content-page narrow center">
      <h1>Page not found</h1>
      <p className="muted">That page does not exist.</p>
      <Link className="primary-button" to="/markets">
        See the markets
      </Link>
    </div>
  );
}
