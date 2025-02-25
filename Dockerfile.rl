FROM rocker/ml 

COPY rl-env.yml environment.yml

#RUN conda update -n base -c conda-forge conda && conda env update --file environment.yml
RUN conda update --all --solver=classic -n base -c conda-forge conda && \
    conda env update --file environment.yml

# Config will be populated by env vars and moved to HOME via start script:
COPY continue-config.json /opt/share/continue/config.json
COPY start.sh /opt/share/start.sh


