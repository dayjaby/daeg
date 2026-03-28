"""Minimal FastAPI server for the polyglot data-science image."""

from pathlib import Path

from fastapi import FastAPI
from fastapi.responses import FileResponse
from fastapi.staticfiles import StaticFiles

app = FastAPI(title="daeg-example")

UI_DIR = Path("/app/ui/dist")


@app.get("/healthz")
async def healthz():
    return {"status": "ok"}


# Serve the React frontend built by Vite.
if UI_DIR.exists():
    app.mount("/", StaticFiles(directory=str(UI_DIR), html=True), name="ui")
