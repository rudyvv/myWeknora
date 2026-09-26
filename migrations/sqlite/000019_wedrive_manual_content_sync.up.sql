UPDATE wedrive_sources SET sync_schedule = '' WHERE sync_schedule <> '';
UPDATE data_sources SET sync_schedule = '' WHERE type = 'wecom_drive_rpa' AND sync_schedule <> '';
