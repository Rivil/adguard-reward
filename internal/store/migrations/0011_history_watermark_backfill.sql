-- 0009 gave every existing child watermark 0, so a child that reused a deleted
-- child's id before 0009 still sees the earlier child's grants. A grant that
-- started before its child existed can only be one of those, so raise such a
-- child's watermark above them. Children created since 0009 read their
-- watermark at insert and are left alone. Only children changes: grants keep
-- every value, end_reason included (locked legacy_end_reason).
UPDATE children
   SET history_after_grant_id = COALESCE(
           (SELECT MAX(g.id)
              FROM grants g
             WHERE g.child_id = children.id
               AND g.started_at < children.created_at), 0)
 WHERE history_after_grant_id = 0;
