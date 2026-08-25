#!/www/server/panel/pyenv/bin/python
"""Point existing Baota New-API tasks at the local-image blue-green commands."""

import sys

sys.path.insert(0, "/www/server/panel/class")

import crontab
import public


BASE_DIR = "/www/dk_project/dk_app/newapi/newapi_TMrS/bluegreen"
TASKS = {
    "New-API 蓝绿更新（手动）": f"{BASE_DIR}/newapi-bluegreen.sh deploy",
    "New-API 蓝绿回滚（手动）": f"{BASE_DIR}/newapi-bluegreen.sh rollback",
}


def main():
    manager = crontab.crontab()
    for name, body in TASKS.items():
        row = public.M("crontab").where("name=?", (name,)).find()
        if not row:
            raise RuntimeError("missing task: {}".format(name))

        row["sBody"] = body
        manager.GetShell(row)
        public.M("crontab").where("id=?", (row["id"],)).setField("sBody", body)
        print("updated id={} status={} {}".format(row["id"], row["status"], name))


if __name__ == "__main__":
    main()
