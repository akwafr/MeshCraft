-- Normalises the reachability notes written BEFORE the loss count was taken
-- out of them.
--
-- `status_note` used to carry "no longer answers ping (3 consecutive losses)".
-- That count grows as long as the device is silent, so a note carrying it
-- changes on every observation — which prevented any deduplication (see
-- store.SetDeviceStatus). It now lives in the log message, where it is
-- informative, and the note describes a stable state.
--
-- WHY A DATA MIGRATION, AND NOT ONLY A SHAPE ONE. Without it, the first
-- re-announcement of each device already unreachable would see a different
-- note, conclude "it changed" and produce two lies at once: a log line
-- announcing a resynchronised switch-over that did not happen, and a
-- `status_at` dated at the update — hence "unreachable for 0 min" on a device
-- down for three days. Nine devices are in that case on the reference
-- installation.
--
-- `status_at` deliberately stays NULL: we do not know since when these findings
-- hold, and fabricating it would be worse than admitting it. The screen then
-- falls back to the note alone, which is exactly the intended behaviour for a
-- row predating the column.
update devices
   set status_note = 'ne répond plus au ping'
 where status_note like 'ne répond plus au ping (%pertes consécutives)';
