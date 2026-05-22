#!/usr/bin/env python3
import sys
import json
import base64
import argparse
from datetime import datetime
from typing import Any, Dict, List, Optional

from textual.app import App, ComposeResult
from textual.widgets import Header, Footer, DataTable, Input, Static
from textual.containers import Horizontal, Vertical, Container
from textual.binding import Binding
from textual.worker import Worker, WorkerState
from textual import work

def format_size(size_bytes: int) -> str:
    if size_bytes == 0:
        return "0B"
    size_name = ("B", "KB", "MB", "GB", "TB", "PB", "EB", "ZB", "YB")
    import math
    i = int(math.floor(math.log(size_bytes, 1024)))
    p = math.pow(1024, i)
    s = round(size_bytes / p, 2)
    return f"{s} {size_name[i]}"

def format_date(date_str: str) -> str:
    try:
        dt = datetime.fromisoformat(date_str.replace("Z", "+00:00"))
        return dt.strftime("%Y-%m-%d %H:%M")
    except Exception:
        return date_str

def osc52_copy(text: str) -> None:
    """Copy text to clipboard using OSC 52 escape sequence."""
    try:
        content = base64.b64encode(text.encode("utf-8")).decode("utf-8")
        # Use sys.__stdout__ to bypass any Textual/Rich redirection
        sys.__stdout__.write(f"\033]52;c;{content}\a")
        sys.__stdout__.flush()
    except Exception:
        pass

class DetailPanel(Static):
    """A panel to display details of the selected item."""
    def update_details(self, item: Dict[str, Any]) -> None:
        self.item = item
        content = f"[bold]Title:[/bold] {item.get('title', 'N/A')}\n"
        content += f"[bold]Info Hash:[/bold] {item.get('infoHash', 'N/A')}\n"
        
        torrent = item.get('torrent', {})
        content += f"[bold]Size:[/bold] {format_size(torrent.get('size', 0))}\n"
        content += f"[bold]File Type:[/bold] {torrent.get('fileType', 'N/A')}\n"
        content += f"[bold]Magnet URI:[/bold] [blue]{torrent.get('magnetUri', 'N/A')}[/blue]\n\n"
        
        if item.get('content'):
            c = item['content']
            content += f"[bold]Content Info:[/bold]\n"
            content += f"  Type: {c.get('type', 'N/A')}\n"
            content += f"  Release Year: {c.get('releaseYear', 'N/A')}\n\n"
        
        content += "[bold]Raw JSON:[/bold]\n"
        content += json.dumps(item, indent=2)
        self.update(content)

class BitmagnetViewer(App):
    TITLE = "Bitmagnet Results Viewer"
    BINDINGS = [
        Binding("q", "quit", "Quit", show=True),
        Binding("f", "focus_filter", "Filter", show=True),
        Binding("d", "toggle_details", "Toggle Details", show=True),
        Binding("c", "copy_magnet", "Copy Magnet", show=True),
        Binding("s", "toggle_sort", "Sort (Seeders)", show=True),
    ]

    def __init__(self, input_source: Any, **kwargs):
        super().__init__(**kwargs)
        self.input_source = input_source
        self.all_items: List[Dict[str, Any]] = []
        self.filtered_items: List[Dict[str, Any]] = []
        self.current_filter = ""
        self.loading_complete = False
        self.sort_descending = True

    def compose(self) -> ComposeResult:
        yield Header()
        yield Input(placeholder="Filter by title...", id="filter-input")
        with Horizontal():
            yield DataTable(id="results-table")
            yield DetailPanel(id="detail-panel", classes="hidden")
        yield Footer()

    def on_mount(self) -> None:
        table = self.query_one(DataTable)
        table.zebra_stripes = True
        table.cursor_type = "row"
        self.setup_table_columns()
        self.load_data()

    def setup_table_columns(self) -> None:
        table = self.query_one(DataTable)
        table.clear(columns=True)
        table.add_column("Title", key="title", width=None) # Flexible
        table.add_column("Type", key="type", width=10)
        table.add_column("Size", key="size", width=10)
        table.add_column("Seeds", key="seeds", width=8)
        table.add_column("Leechs", key="leechs", width=8)
        table.add_column("Published", key="published", width=18)

    @work(exclusive=True, thread=True)
    def load_data(self) -> None:
        batch_size = 100
        batch = []
        count = 0
        
        try:
            self.call_from_thread(self.update_status, "Starting data load...")
            for line in self.input_source:
                line = line.strip()
                if not line:
                    continue
                try:
                    item = json.loads(line)
                    # Use a stable key for the row
                    item_id = item.get("id") or f"row-{count}"
                    item["id"] = item_id # Ensure it has an id
                    
                    self.all_items.append(item)
                    batch.append(item)
                    count += 1
                    
                    if len(batch) >= batch_size:
                        self.call_from_thread(self.add_batch_to_table, list(batch))
                        batch = []
                        self.call_from_thread(self.update_status, f"Loading... ({count} items)")
                except json.JSONDecodeError as je:
                    self.call_from_thread(self.update_status, f"JSON Error on line {count+1}")
                    continue
            
            if batch:
                self.call_from_thread(self.add_batch_to_table, list(batch))
            
            self.loading_complete = True
            self.call_from_thread(self.update_status, f"Loaded {count} items")
        except Exception as e:
            self.call_from_thread(self.update_status, f"Error: {str(e)}")

    def add_batch_to_table(self, items: List[Dict[str, Any]]) -> None:
        table = self.query_one(DataTable)
        for item in items:
            torrent = item.get("torrent", {})
            size = format_size(torrent.get("size", 0))
            seeders = item.get('seeders')
            leechers = item.get('leechers')
            s = str(seeders) if seeders is not None else "?"
            l = str(leechers) if leechers is not None else "?"
            published = format_date(item.get("publishedAt", ""))
            
            table.add_row(
                item.get("title", "N/A"),
                item.get("contentType") or "N/A",
                size,
                s,
                l,
                published,
                key=item.get("id")
            )

    def update_status(self, message: str) -> None:
        self.sub_title = message

    def on_input_changed(self, event: Input.Changed) -> None:
        if event.input.id == "filter-input":
            self.current_filter = event.value.lower()
            self.filter_results()

    def filter_results(self) -> None:
        table = self.query_one(DataTable)
        table.clear()
        
        self.filtered_items = [
            item for item in self.all_items
            if self.current_filter in item.get("title", "").lower()
        ]
        
        if self.sort_descending is not None:
            self.filtered_items.sort(
                key=lambda x: x.get("seeders") if x.get("seeders") is not None else -1,
                reverse=self.sort_descending
            )
        
        # Batch add filtered results
        batch_size = 1000
        for i in range(0, len(self.filtered_items), batch_size):
            self.add_batch_to_table(self.filtered_items[i:i + batch_size])

    def action_toggle_sort(self) -> None:
        if self.sort_descending is None:
            self.sort_descending = True
        else:
            self.sort_descending = not self.sort_descending
        
        sort_mode = "Seeders Desc" if self.sort_descending else "Seeders Asc"
        self.notify(f"Sorting by {sort_mode}")
        self.filter_results()

    def on_data_table_row_selected(self, event: DataTable.RowSelected) -> None:
        if event.row_key:
            self.update_details(event.row_key.value)
            # Also ensure panel is visible if hidden
            panel = self.query_one(DetailPanel)
            if "hidden" in panel.classes:
                panel.remove_class("hidden")

    def on_data_table_row_highlighted(self, event: DataTable.RowHighlighted) -> None:
        if event.row_key:
            self.update_details(event.row_key.value)

    def update_details(self, item_id: str) -> None:
        item = next((i for i in self.all_items if i.get("id") == item_id), None)
        if item:
            self.query_one(DetailPanel).update_details(item)

    def action_toggle_details(self) -> None:
        panel = self.query_one(DetailPanel)
        if "hidden" in panel.classes:
            panel.remove_class("hidden")
        else:
            panel.add_class("hidden")

    def action_focus_filter(self) -> None:
        self.query_one("#filter-input").focus()

    def action_copy_magnet(self) -> None:
        table = self.query_one(DataTable)
        try:
            # Get the row key for the currently highlighted row using cursor coordinate
            coord = table.cursor_coordinate
            cell_key = table.coordinate_to_cell_key(coord)
            row_key = cell_key.row_key
            
            if row_key:
                item_id = row_key.value
                item = next((i for i in self.all_items if i.get("id") == item_id), None)
                if item:
                    magnet = item.get("torrent", {}).get("magnetUri")
                    if magnet:
                        osc52_copy(magnet)
                        self.notify(f"Copied magnet for: {item.get('title')[:30]}...", title="Clipboard")
                        return
            
            self.notify("No magnet URI found for selected row", severity="error")
        except Exception as e:
            self.notify(f"Copy failed: {str(e)}", severity="error")

    def action_quit(self) -> None:
        self.exit()

CSS = """
#filter-input {
    margin: 1;
}

#results-table {
    height: 1fr;
    width: 1fr;
}

#detail-panel {
    height: 1fr;
    width: 50;
    border-left: solid $accent;
    padding: 1;
    overflow-y: scroll;
}

.hidden {
    display: none;
}

DataTable {
    background: $surface;
    color: $text;
}

DetailPanel {
    background: $surface;
}
"""

def main():
    parser = argparse.ArgumentParser(description="TUI viewer for Bitmagnet search results.")
    parser.add_argument("file", nargs="?", help="JSONL file to view (default: stdin)")
    args = parser.parse_args()

    input_data = []
    if args.file:
        try:
            with open(args.file, "r") as f:
                app = BitmagnetViewer(f)
                app.run()
        except FileNotFoundError:
            print(f"Error: File not found: {args.file}")
            sys.exit(1)
    else:
        if sys.stdin.isatty():
            print("Usage: bitmagnet_viewer.py [file.jsonl] or pipe data into it.")
            sys.exit(1)
        # Read all of stdin before starting the app to avoid conflicts with TUI stdin
        stdin_data = sys.stdin.read().splitlines()
        app = BitmagnetViewer(stdin_data)
        app.run()

if __name__ == "__main__":
    BitmagnetViewer.CSS = CSS
    main()
