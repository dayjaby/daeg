import { useState, useEffect } from "react";

export default function App() {
  const [status, setStatus] = useState("loading...");

  useEffect(() => {
    fetch("/healthz")
      .then((r) => r.json())
      .then((data) => setStatus(data.status))
      .catch(() => setStatus("unreachable"));
  }, []);

  return (
    <main style={{ fontFamily: "system-ui", padding: "2rem" }}>
      <h1>daeg example</h1>
      <p>
        Backend: <strong>{status}</strong>
      </p>
      <p style={{ color: "#666", fontSize: "0.9rem" }}>
        CUDA, OpenCV, FFmpeg, FastAPI, and React — built with parallel DAG
        layers.
      </p>
    </main>
  );
}
