/**
 * Copyright (c) 2023-present Plane Software, Inc. and contributors
 * SPDX-License-Identifier: AGPL-3.0-only
 * See the LICENSE file for details.
 */

import type { CoreRootStore } from "../root.store";
import type { IProjectStore } from "./project.store";
import { ProjectStore } from "./project.store";
import type { IProjectFilterStore } from "./project_filter.store";
import { ProjectFilterStore } from "./project_filter.store";

export interface IProjectRootStore {
  project: IProjectStore;
  projectFilter: IProjectFilterStore;
}

export class ProjectRootStore {
  project: IProjectStore;
  projectFilter: IProjectFilterStore;

  constructor(_root: CoreRootStore) {
    this.project = new ProjectStore(_root);
    this.projectFilter = new ProjectFilterStore(_root);
  }
}
