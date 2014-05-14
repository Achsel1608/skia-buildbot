#!/usr/bin/env python
# Copyright (c) 2014 The Chromium Authors. All rights reserved.
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.

"""Generate Doxygen documentation."""

import datetime
import os
import shutil
import sys

from build_step import BuildStep
from utils import file_utils, shell_utils

DOXYFILE_BASENAME = 'Doxyfile'  # must match name of Doxyfile in skia root
DOXYGEN_BINARY = 'doxygen'
DOXYGEN_CONFIG_DIR = os.path.join(os.pardir, os.pardir, 'doxygen-config')
DOXYGEN_WORKING_DIR = os.path.join(os.pardir, os.pardir, 'doxygen')

IFRAME_FOOTER_TEMPLATE = """
<html><body><address style="text-align: right;"><small>
Generated at %s for skia
by <a href="http://www.doxygen.org/index.html">doxygen</a>
%s </small></address></body></html>
"""


class GenerateDoxygen(BuildStep):
  def _Run(self):
    # Create empty dir and add static_footer.txt
    file_utils.create_clean_local_dir(DOXYGEN_WORKING_DIR)
    static_footer_path = os.path.join(DOXYGEN_WORKING_DIR, 'static_footer.txt')
    shutil.copyfile(os.path.join('tools', 'doxygen_footer.txt'),
                    static_footer_path)

    # Make copy of doxygen config file, overriding any necessary configs,
    # and run doxygen.
    file_utils.create_clean_local_dir(DOXYGEN_CONFIG_DIR)
    modified_doxyfile = os.path.join(DOXYGEN_CONFIG_DIR, DOXYFILE_BASENAME)
    with open(DOXYFILE_BASENAME, 'r') as reader:
      with open(modified_doxyfile, 'w') as writer:
        shutil.copyfileobj(reader, writer)
        writer.write('OUTPUT_DIRECTORY = %s\n' % DOXYGEN_WORKING_DIR)
        writer.write('HTML_FOOTER = %s\n' % static_footer_path)
    shell_utils.run([DOXYGEN_BINARY, modified_doxyfile])

    # Create iframe_footer.html
    with open(os.path.join(DOXYGEN_WORKING_DIR, 'iframe_footer.html'),
              'w') as fh:
      fh.write(IFRAME_FOOTER_TEMPLATE % (
          datetime.datetime.now().isoformat(' '),
          shell_utils.run([DOXYGEN_BINARY, '--version'])))


if '__main__' == __name__:
  sys.exit(BuildStep.RunBuildStep(GenerateDoxygen))
