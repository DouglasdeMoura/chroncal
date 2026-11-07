# Palette Audit

This file audits the palette delta. The file lists each delta command. The file gives a status. The file gives a file reference. The audit checks palette_commands.go. The audit checks palette.go. The audit checks palette_search.go. The engine starts only after this file exists.

| Command | Status | Reference |
|---|---|---|
| today | exists | palette_commands.go GoToTodayMsg |
| goto date | missing | new GoToDateMsg in palette_commands.go |
| view day week month agenda | exists | PaletteCommand view.month view.week view.day view.agenda |
| next event | missing | new NextEventMsg in palette_commands.go |
| prev event | missing | new PrevEventMsg in palette_commands.go |
| create at slot | partial | EventCreateMsg{Day} in event_dialog.go, new Start field needed |
| calendar | out of wedge | exists as CalendarManagerRequestedMsg, no delta |
| fuzzyScore | exists | palette.go fuzzyScore |
| MRU | out of wedge | no delta |

The wedge is five commands. The wedge ships only today goto view next prev. The create slot needs a Start field. The audit found no need to rebuild palette.go.
