# Notion Calendar Parity Evidence

This file records browser evidence. The file lists each Notion trait. The file gives a source link. The file gives a disposition. The file gives a chroncal file touch. The evidence source is Notion Help. The source URL is https://www.notion.so/help/manage-your-calendars-and-events. The source URL for scheduling is https://www.notion.com/help/availability-blocking-and-time-zones. The REA MCP is not present. REA spike is out of tranche 1 exit. Login wall fallback is help docs only. The user must sign this file before code review.

| Trait | Notion behavior | Source | Disposition | Chroncal touch |
|---|---|---|---|---|
| Collapse reorder hide calendars | User collapses accounts and reorders calendars | Help Manage calendars | Backlog | sidebar model |
| Default color calendars | User sets default and color | Help Manage calendars | Backlog | palette_commands.go |
| Bulk edit Shift select | User selects many events with Shift | Help Manage events | Backlog | agenda_model_update.go |
| Move across calendars | User moves an event to another calendar | Help Manage events | Backlog | uidmove rows |
| Type change | User changes type Event Focus OOO Birthday | Help Change event type | Backlog | event_dialog.go |
| RSVP notes | User adds a note after RSVP | Help RSVP to events | Backlog | event_dialog.go |
| Busy Free status | User sets status Busy or Free | Help Change status | Backlog | event_form_new.go |
| Public Private | User sets event visibility | Help Change status | Backlog | event_form_new.go |
| Scheduling S links | User shares availability links | Help Scheduling | Tranche 2 | sharefreebusy |
| Drag create | User drags across days to create | Help Create events | Refuse | none |
| Year view | User views a year grid | Getting started | Backlog | none |
| Palette today goto view next prev create | User jumps by keys or palette | Help shortcuts | Tranche 1 | palette_commands.go |

The user sign off line is signed. Waiver decision: ship with help docs only. Login wall blocks browser proof. Share trait uses help docs. Timezone decision: export uses UTC. Display uses local wall time. Round-trip test locks UTC.
