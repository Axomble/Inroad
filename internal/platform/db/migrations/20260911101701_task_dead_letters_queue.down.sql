-- Drop the captured queue. The column's CHECK goes with it (a constraint cannot
-- outlive its column), so nothing else has to be re-added here; replay falls
-- back to reconstructing the route from the task type, which is what every row
-- captured before this migration already does.
ALTER TABLE task_dead_letters
    DROP COLUMN queue;
